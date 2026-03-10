# Networking workspace target
# Resources split from monolith - networking only

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Same resources, same names - direct match for autosuggest
resource "null_resource" "vpc" {
  triggers = {
    name = "main-vpc"
    cidr = "10.0.0.0/16"
  }
}

resource "null_resource" "subnet_public" {
  triggers = {
    name = "public-subnet"
    cidr = "10.0.1.0/24"
  }
}

resource "null_resource" "security_group_web" {
  triggers = {
    name  = "web-sg"
    ports = "80,443"
  }
}
