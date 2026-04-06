// control-plane/internal/models/store.go
package models

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the interface for model metadata persistence.
type Store interface {
	// CreateOrGetModelRecord upserts a model_record row. On conflict (tenant_id, name),
	// returns the existing record. rec.ID is always set on return.
	CreateOrGetModelRecord(ctx context.Context, rec *ModelRecord) error
	// CreateModelVersion inserts a new model_versions row. ver.ID is set on return.
	CreateModelVersion(ctx context.Context, ver *ModelVersion) error
	// GetModelRecordByName returns the model record for the given name and tenant.
	GetModelRecordByName(ctx context.Context, name, tenantID string) (*ModelRecord, error)
	// ListModelVersions returns all versions for the given model record, ordered by version_number ASC.
	ListModelVersions(ctx context.Context, modelRecordID string) ([]*ModelVersion, error)
	// GetModelVersionByNumber returns a specific version of a model record.
	GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*ModelVersion, error)
	// UpdateModelVersionStatus sets the status field on a model version.
	// Used for terminal transitions (archived) where no alias clearing is needed.
	UpdateModelVersionStatus(ctx context.Context, id, status string) error
	// PromoteModelVersionStatus atomically clears all other versions that hold the given alias
	// status for this model record, then sets versionID's status to alias. Used for staging and
	// production promotions to maintain the invariant that only one version holds each alias status.
	PromoteModelVersionStatus(ctx context.Context, modelRecordID, versionID, alias string) error
}

// PostgresModelStore implements Store against PostgreSQL.
type PostgresModelStore struct {
	db *pgxpool.Pool
}

// NewPostgresModelStore returns a Store backed by the given pool.
func NewPostgresModelStore(db *pgxpool.Pool) Store {
	return &PostgresModelStore{db: db}
}

func (s *PostgresModelStore) CreateOrGetModelRecord(ctx context.Context, rec *ModelRecord) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO model_records (tenant_id, project_id, name, mlflow_registered_model_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, name) DO UPDATE
		  SET updated_at = model_records.updated_at
		RETURNING id::text, tenant_id::text, project_id::text, name,
		          mlflow_registered_model_name, created_at, updated_at`,
		rec.TenantID, rec.ProjectID, rec.Name, rec.MLflowRegisteredModelName,
	).Scan(
		&rec.ID, &rec.TenantID, &rec.ProjectID, &rec.Name,
		&rec.MLflowRegisteredModelName, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert model_record: %w", err)
	}
	return nil
}

func (s *PostgresModelStore) CreateModelVersion(ctx context.Context, ver *ModelVersion) error {
	err := s.db.QueryRow(ctx, `
		INSERT INTO model_versions
		  (model_record_id, tenant_id, version_number, mlflow_run_id, source_run_id, artifact_uri, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, model_record_id::text, tenant_id::text, version_number,
		          mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at`,
		ver.ModelRecordID, ver.TenantID, ver.VersionNumber,
		ver.MLflowRunID, ver.SourceRunID, ver.ArtifactURI, ver.Status,
	).Scan(
		&ver.ID, &ver.ModelRecordID, &ver.TenantID, &ver.VersionNumber,
		&ver.MLflowRunID, &ver.SourceRunID, &ver.ArtifactURI, &ver.Status,
		&ver.CreatedAt, &ver.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrVersionAlreadyExists
		}
		return fmt.Errorf("insert model_version: %w", err)
	}
	return nil
}

func (s *PostgresModelStore) GetModelRecordByName(ctx context.Context, name, tenantID string) (*ModelRecord, error) {
	var rec ModelRecord
	err := s.db.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, project_id::text, name,
		       mlflow_registered_model_name, created_at, updated_at
		FROM model_records
		WHERE name = $1 AND tenant_id = $2`,
		name, tenantID,
	).Scan(
		&rec.ID, &rec.TenantID, &rec.ProjectID, &rec.Name,
		&rec.MLflowRegisteredModelName, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrModelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get model_record: %w", err)
	}
	return &rec, nil
}

func (s *PostgresModelStore) ListModelVersions(ctx context.Context, modelRecordID string) ([]*ModelVersion, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, model_record_id::text, tenant_id::text, version_number,
		       mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at
		FROM model_versions
		WHERE model_record_id = $1
		ORDER BY version_number ASC`,
		modelRecordID,
	)
	if err != nil {
		return nil, fmt.Errorf("list model_versions: %w", err)
	}
	defer rows.Close()

	var result []*ModelVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *PostgresModelStore) GetModelVersionByNumber(ctx context.Context, modelRecordID string, versionNumber int) (*ModelVersion, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id::text, model_record_id::text, tenant_id::text, version_number,
		       mlflow_run_id, source_run_id::text, artifact_uri, status, created_at, updated_at
		FROM model_versions
		WHERE model_record_id = $1 AND version_number = $2`,
		modelRecordID, versionNumber,
	)
	ver, err := scanVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	return ver, err
}

func (s *PostgresModelStore) UpdateModelVersionStatus(ctx context.Context, id, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE model_versions SET status = $1, updated_at = now() WHERE id = $2`,
		status, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionNotFound
	}
	return nil
}

func (s *PostgresModelStore) PromoteModelVersionStatus(ctx context.Context, modelRecordID, versionID, alias string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Clear all other versions of this model that currently hold the same alias.
	_, err = tx.Exec(ctx,
		`UPDATE model_versions SET status = 'candidate', updated_at = now()
		 WHERE model_record_id = $1 AND status = $2 AND id != $3::uuid`,
		modelRecordID, alias, versionID,
	)
	if err != nil {
		return fmt.Errorf("clear prior alias holders: %w", err)
	}

	// Set the target version's status.
	tag, err := tx.Exec(ctx,
		`UPDATE model_versions SET status = $1, updated_at = now() WHERE id = $2::uuid`,
		alias, versionID,
	)
	if err != nil {
		return fmt.Errorf("set version status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionNotFound
	}

	return tx.Commit(ctx)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanVersion(row scannable) (*ModelVersion, error) {
	var v ModelVersion
	err := row.Scan(
		&v.ID, &v.ModelRecordID, &v.TenantID, &v.VersionNumber,
		&v.MLflowRunID, &v.SourceRunID, &v.ArtifactURI, &v.Status,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
