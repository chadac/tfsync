{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "tfsync";
  version = "0.1.0";
  src = ./.;
  vendorHash = "sha256-x2EUobyyM2PzbhwymZ+gpl92Z6XJre7u+8cdT7/k7UE=";
  subPackages = [ "cmd" ];

  postInstall = ''
    mv $out/bin/cmd $out/bin/tfsync
  '';

  meta = with lib; {
    description = "Terraform State Migration Validator";
    homepage = "https://github.com/chadac/tfsync";
    license = licenses.mit;
    mainProgram = "tfsync";
  };
}
