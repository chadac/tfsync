# tfsync

Validate Terraform state migrations before applying them to production.

## Overview

tfsync helps you safely test state migrations by:

1. Copying state from your source terraform configuration to a local target
2. Running your configured state moves/migrations
3. Running `terraform plan` to verify no infrastructure changes

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
tfsync

# Specify config file
tfsync -c my-migration.yaml

# Dry run - show what would be done
tfsync --dry-run

# Verbose output
tfsync -v

# Keep generated files after run (for debugging)
tfsync --no-cleanup
```

## Configuration

Create a `tfsync.yaml` in your project:

```yaml
version: "1"

# Source: where to pull state from (uses its configured backend)
source:
  path: ./terraform/old

# Target: where to test the migration (uses local state)
target:
  path: ./terraform/new

# Migration: how to transform the state
migration:
  moves:
    - from: "aws_s3_bucket.data"
      to: "module.storage.aws_s3_bucket.main"
    - from: "aws_iam_role.app"
      to: "module.iam.aws_iam_role.application"

# Optional: tf tool settings
tf:
  tool: tofu  # or "terraform"
```

### Multi-workspace migrations

#### Splitting one state into multiple workspaces

Split a monolith state into multiple target workspaces:

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

#### Migrating between matching workspaces

When both source and target have workspaces with matching names, each source workspace is copied to its corresponding target:

```yaml
version: "1"

source:
  workspaces:
    prod:
      path: ./old-terraform/prod
    staging:
      path: ./old-terraform/staging

target:
  workspaces:
    prod:
      path: ./new-terraform/prod
    staging:
      path: ./new-terraform/staging

migration:
  moves:
    - from: "aws_s3_bucket.data"
      to: "module.storage.aws_s3_bucket.main"
```

The `to` field supports two formats:
- **Simple string**: `to: "module.foo.resource_name"` - just the destination address
- **Object**: `to: { workspace: "name", resource: "address" }` - destination with workspace

### Using a migration script

For complex migrations, use an external script:

```yaml
migration:
  script: ./migrate.sh
```

The script receives these environment variables:
- `TFSYNC_TARGET_DIR` - Path to the target directory
- `TFSYNC_TF_BINARY` - The terraform binary being used (tofu/terraform)

## How it works

1. **Copy state**: Pulls state from source's configured backend and writes it as a local state file in the target directory
2. **Override backend**: Creates a `backend_override.tf` to force local state in target
3. **Run migrations**: Executes configured `terraform state mv` commands or your migration script
4. **Validate**: Runs `terraform plan` - success means no infrastructure changes
5. **Cleanup**: Removes generated files (unless `--no-cleanup`)

## License

MIT
