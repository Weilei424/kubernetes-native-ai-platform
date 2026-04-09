// control-plane/internal/deployments/store.go
package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the interface for deployment metadata persistence.
type Store interface {
	// CreateDeployment inserts a new deployment and its first revision atomically.
	// d.ID is set on return. Returns ErrDuplicateDeploymentName on unique constraint violation.
	CreateDeployment(ctx context.Context, d *Deployment) error
	// GetDeployment returns the deployment with the given id.
	// Returns ErrDeploymentNotFound if not found.
	GetDeployment(ctx context.Context, id string) (*Deployment, error)
	// UpdateDeploymentStatus updates the status and serving_endpoint of a deployment.
	UpdateDeploymentStatus(ctx context.Context, id, status, endpoint string) error
	// DeleteDeployment sets the deployment status to "deleted".
	// Returns ErrDeploymentNotFound if no deployment with the given id exists
	// or if the deployment is already deleted.
	DeleteDeployment(ctx context.Context, id string) error
	// ListPendingDeployments returns all deployments with status "pending" or "provisioning",
	// enriched with artifact_uri and model_name via JOIN for operator consumption.
	ListPendingDeployments(ctx context.Context) ([]*Deployment, error)
}

// PostgresDeploymentStore implements Store against PostgreSQL.
type PostgresDeploymentStore struct {
	db *pgxpool.Pool
}

// NewPostgresDeploymentStore returns a Store backed by the given pool.
func NewPostgresDeploymentStore(db *pgxpool.Pool) Store {
	return &PostgresDeploymentStore{db: db}
}

func (s *PostgresDeploymentStore) CreateDeployment(ctx context.Context, d *Deployment) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO deployments
		  (tenant_id, project_id, model_record_id, model_version_id, name, namespace, status, desired_replicas)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id::text, tenant_id::text, project_id::text, model_record_id::text,
		          model_version_id::text, name, namespace, status, desired_replicas,
		          COALESCE(serving_endpoint, ''), created_at, updated_at`,
		d.TenantID, d.ProjectID, d.ModelRecordID, d.ModelVersionID,
		d.Name, d.Namespace, d.Status, d.DesiredReplicas,
	).Scan(
		&d.ID, &d.TenantID, &d.ProjectID, &d.ModelRecordID,
		&d.ModelVersionID, &d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
		&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateDeploymentName
		}
		return fmt.Errorf("insert deployment: %w", err)
	}

	// Insert revision 1 atomically with the deployment.
	_, err = tx.Exec(ctx, `
		INSERT INTO deployment_revisions (deployment_id, revision_number, model_version_id, status)
		VALUES ($1, 1, $2, 'active')`,
		d.ID, d.ModelVersionID,
	)
	if err != nil {
		return fmt.Errorf("insert deployment_revision: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PostgresDeploymentStore) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, model_record_id::text,
		       model_version_id::text, name, namespace, status, desired_replicas,
		       COALESCE(serving_endpoint, ''), created_at, updated_at
		FROM deployments WHERE id = $1::uuid`, id,
	).Scan(
		&d.ID, &d.TenantID, &d.ProjectID, &d.ModelRecordID,
		&d.ModelVersionID, &d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
		&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeploymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return &d, nil
}

func (s *PostgresDeploymentStore) UpdateDeploymentStatus(ctx context.Context, id, status, endpoint string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE deployments
		SET status = $1, serving_endpoint = NULLIF($2, ''), updated_at = now()
		WHERE id = $3::uuid`,
		status, endpoint, id,
	)
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentNotFound
	}
	return nil
}

func (s *PostgresDeploymentStore) DeleteDeployment(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE deployments SET status = 'deleted', updated_at = now()
		WHERE id = $1::uuid AND status != 'deleted'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete deployment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentNotFound
	}
	return nil
}

func (s *PostgresDeploymentStore) ListPendingDeployments(ctx context.Context) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, d.tenant_id::text, d.project_id::text,
		       d.model_record_id::text, d.model_version_id::text,
		       d.name, d.namespace, d.status, d.desired_replicas,
		       COALESCE(d.serving_endpoint, ''), d.created_at, d.updated_at,
		       mv.artifact_uri, mr.name
		FROM deployments d
		JOIN model_versions mv ON mv.id = d.model_version_id
		JOIN model_records  mr ON mr.id = d.model_record_id
		WHERE d.status IN ('pending', 'provisioning')`)
	if err != nil {
		return nil, fmt.Errorf("list pending deployments: %w", err)
	}
	defer rows.Close()

	result := make([]*Deployment, 0)
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.ProjectID,
			&d.ModelRecordID, &d.ModelVersionID,
			&d.Name, &d.Namespace, &d.Status, &d.DesiredReplicas,
			&d.ServingEndpoint, &d.CreatedAt, &d.UpdatedAt,
			&d.ArtifactURI, &d.ModelName,
		); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result = append(result, &d)
	}
	return result, rows.Err()
}
