# tfsync

Validate Terraform state migrations before applying them to production.

## Overview

tfsync helps you safely test state migrations by:

1. Pulling state from your source terraform configuration(s)
2. Copying state to target workspace(s) with a local backend override
3. Executing configured state moves/migrations
4. Running `terraform plan` to verify no infrastructure changes

If the plan shows no changes, your migration is correct!

## Installation

### Using Nix (recommended)

```bash
# Run directly
nix run github:chadac/tfsync

# Or add to your flake
{
  inputs.tfsync.url = "github:chadac/tfsync";
}
```

### From source

```bash
go install github.com/chadac/tfsync/cmd/tfsync@latest
```

## Usage

```bash
# Run with default config (tfsync.yaml)
tfsync run

# Run specific target workspaces only
tfsync run networking compute

# Force refresh state from source (ignore cache)
tfsync run -r

# Specify config file
tfsync run -c my-migration.yaml

# Verbose output
tfsync run -v

# Debug mode (show all terraform commands)
tfsync run --debug

# Dry run - show plan without executing
tfsync run --dry-run

# Auto-suggest migrations based on resource similarity
tfsync autosuggest
```

## Configuration

Create a `tfsync.yaml` in your project:

### Simple migration (single source, single target)

```yaml
version: "1"

source:
  path: ./terraform/old

target:
  path: ./terraform/new

migration:
  moves:
    - from: "aws_s3_bucket.data"
      to: "module.storage.aws_s3_bucket.main"
    - from: "aws_iam_role.app"
      to: "module.iam.aws_iam_role.application"

tf:
  tool: tofu  # or "terraform" (default: tofu)
```

### Multi-workspace migrations

#### Splitting one state into multiple workspaces

```yaml
version: "1"

source:
  path: ./terraform/monolith

target:
  workspaces:
    networking:
      path: ./terraform/networking
    compute:
      path: ./terraform/compute

migration:
  moves:
    - from: "aws_vpc.main"
      to: { workspace: networking, resource: "aws_vpc.main" }
    - from: "aws_instance.app"
      to: { workspace: compute, resource: "aws_instance.app" }
```

#### Multiple source workspaces to multiple targets

```yaml
version: "1"

source:
  workspaces:
    production:
      path: ./old-terraform/production
    shared:
      path: ./old-terraform/shared

target:
  workspaces:
    networking:
      path: ./new-terraform/networking
      prefer_workspace: production
    compute:
      path: ./new-terraform/compute
      prefer_workspace: production

migration:
  moves:
    - from: { workspace: production, resource: "aws_vpc.main" }
      to: { workspace: networking, resource: "aws_vpc.main" }
    - from: { workspace: production, resource: "aws_instance.app" }
      to: { workspace: compute, resource: "aws_instance.app" }
```

The `from` and `to` fields support two formats:
- **Simple string**: `"module.foo.resource_name"` — just the resource address
- **Object**: `{ workspace: "name", resource: "address" }` — with explicit workspace

When source workspaces are defined, `prefer_workspace` on a target specifies which source to pull state from. If omitted, tfsync matches by workspace name.

### Module moves

Move entire modules at once:

```yaml
migration:
  moves:
    - from: "module.old_network"
      to: { workspace: networking, resource: "module.network" }
```

This moves all resources under the source module to the target, updating module paths accordingly.

### Migration scripts

For complex migrations, use an external script:

```yaml
migration:
  script: ./migrate.sh
```

The script receives these environment variables:
- `TFSYNC_TARGET_DIR` — Path to the target directory
- `TFSYNC_TF_BINARY` — The terraform binary being used (tofu/terraform)

### Provider remapping

When source and target use different provider configurations (e.g., aliased providers), use `provider_remap`:

```yaml
migration:
  # Global provider remap (applies to all workspaces)
  provider_remap:
    "aws.ireland": "aws"

target:
  workspaces:
    networking:
      path: ./terraform/networking
      # Per-workspace override (takes precedence over global)
      provider_remap:
        "aws.ireland": "aws.eu_west_1"
```

Provider remaps use short names (e.g., `aws.ireland`) which map to the full provider address in state (e.g., `provider["registry.terraform.io/hashicorp/aws"].ireland`).

### Plan configuration

Configure how `terraform plan` runs for target workspaces:

```yaml
target:
  workspaces:
    networking:
      path: ./terraform/networking
      plan:
        # State locking (default: false for validation plans)
        lock: false

        # Extra arguments passed to terraform plan
        extra_args: ["-no-color"]

        # Override files written before planning (e.g., provider config)
        override_files:
          provider_override.tf: |
            provider "aws" {
              region = "us-east-1"
            }

        # Ignore plan changes for specific resources
        ignore_changes:
          - "aws_s3_bucket.legacy_data"
          - "module.vpc.aws_route_table.private"

        # Ignore plan errors for specific resources (e.g., refresh failures)
        ignore_errors:
          - "module.stack.aws_s3_object.data_folder[0]"
```

**`ignore_changes`**: If the plan shows changes but all changed resources are in this list, the plan is considered passing.

**`ignore_errors`**: If the plan fails (exit code 1) but all errors are for resources in this list, the plan is considered passing. Useful when resources fail during refresh (e.g., S3 403 Forbidden) but the migration itself is correct.

**`lock`**: Defaults to `false` for validation plans to avoid lock contention when running plans in parallel. Set to `true` if you need state locking.

> **Note:** `plan.refresh = false` is not allowed on target workspaces. tfsync validates migrations by running a real plan against the migrated state, which requires refresh.

### Workspace dependencies

Control the order in which target workspace plans run:

```yaml
target:
  workspaces:
    networking:
      path: ./terraform/networking

    compute:
      path: ./terraform/compute
      depends_on:
        - networking

    monitoring:
      path: ./terraform/monitoring
      depends_on:
        - compute
        - networking
```

Workspaces are planned in dependency order (topological sort). Workspaces at the same level run in parallel. This is useful when one workspace's `terraform plan` reads another workspace's remote state.

Dependencies only affect plan ordering — state migrations run independently.

### Init configuration

Configure `terraform init` for source or target workspaces:

```yaml
source:
  workspaces:
    production:
      path: ./terraform/production
      init:
        backend:
          bucket: "my-state-bucket"
          key: "prod/terraform.tfstate"
        extra_args: ["-upgrade"]
```

### Global TF settings

```yaml
tf:
  tool: tofu           # terraform binary (default: tofu)
  parallelism: 10      # -parallelism flag for plan
  var_files:           # -var-file flags for plan
    - vars.tfvars
    - prod.tfvars
  vars:                # -var flags for plan
    - "region=us-east-1"
```

## How it works

1. **Pull state**: Pulls state from each source workspace's configured backend (cached locally for subsequent runs)
2. **Write to targets**: Copies source state to each target directory as `terraform.tfstate` with a `backend_override.tf` to force local state
3. **Build migration plan**: Validates all moves, checks for duplicates, orphans, and provider mismatches
4. **Execute migrations**: Builds new state files for each target containing only their assigned resources (with renames, module moves, and provider remaps applied)
5. **Initialize targets**: Runs `terraform init` on each target workspace
6. **Validate plans**: Runs `terraform plan` on each target in dependency order — success means no infrastructure changes

### State deduplication

tfsync automatically deduplicates resource instances when loading state files. Some corrupted state files contain multiple instances with the same `index_key` — tfsync keeps the last occurrence for each key.

## License

MIT
