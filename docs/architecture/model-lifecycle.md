# Model Lifecycle

```mermaid
stateDiagram-v2
    [*] --> candidate : POST /v1/models\n(register from succeeded run)
    candidate --> staging   : promote alias=staging
    candidate --> archived  : promote alias=archived
    staging    --> production : promote alias=production
    staging    --> archived  : promote alias=archived
    production --> archived  : promote alias=archived
    archived   --> [*]
```

**Promotion rules:**
- `archived` is terminal — cannot be re-promoted (returns 409)
- Promoting a version to `production` demotes the prior `production` version to `staging`
- Only `production`-aliased versions can be deployed
