# Simple target configuration - refactored into modules
# Same resources but with different addresses

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Moved to module structure
module "data" {
  source = "./modules/data"
}

module "compute" {
  source = "./modules/compute"
}
