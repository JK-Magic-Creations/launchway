terraform {
  required_providers {
    random = { source = "opentofu/random", version = "~> 3.6" }
    null   = { source = "opentofu/null", version = "~> 3.2" }
  }
}

variable "name" {
  type    = string
  default = "web"
}

variable "pet_count" {
  type    = number
  default = 2
}

resource "random_pet" "server" {
  count  = var.pet_count
  length = 2
  prefix = var.name
}

resource "random_password" "db" {
  length  = 20
  special = false
}

resource "null_resource" "marker" {
  triggers = {
    name = var.name
  }
}

output "pets" {
  value = random_pet.server[*].id
}

output "db_password" {
  value     = random_password.db.result
  sensitive = true
}
