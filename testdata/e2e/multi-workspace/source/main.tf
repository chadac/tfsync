# Multi-workspace source - monolith that will be split
# Contains both networking and compute resources

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Networking resources
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

resource "null_resource" "subnet_private" {
  triggers = {
    name = "private-subnet"
    cidr = "10.0.2.0/24"
  }
}

# Compute resources
resource "null_resource" "web_server" {
  triggers = {
    name = "web-1"
    type = "t3.medium"
  }
}

resource "null_resource" "api_server" {
  triggers = {
    name = "api-1"
    type = "t3.large"
  }
}
