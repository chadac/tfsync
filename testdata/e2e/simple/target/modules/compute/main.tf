# Compute module - contains app server resources

resource "null_resource" "app_server" {
  triggers = {
    name = "web-server"
  }
}
