{
  description = "tfsync - Terraform State Migration Validator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { config, self', inputs', pkgs, system, ... }: {
        packages = {
          tfsync = pkgs.buildGoModule {
            pname = "tfsync";
            version = "0.1.0";
            src = ./.;
            vendorHash = "sha256-Y8z+13XK40p36EYg5Nam8Ds8EW/OIStbpawl2sqOhrY=";
            subPackages = [ "cmd" ];
            meta = with pkgs.lib; {
              description = "Terraform State Migration Validator";
              homepage = "https://github.com/chadac/tfsync";
              license = licenses.mit;
              maintainers = [ ];
            };
          };
          default = self'.packages.tfsync;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
            go-tools
            delve
            opentofu
          ];
        };
      };
    };
}
