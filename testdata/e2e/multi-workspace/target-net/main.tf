# Networking workspace - receives VPC and subnet resources

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

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
