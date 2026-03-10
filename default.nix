{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "tfsync";
  version = "0.1.0";
  src = ./.;
  vendorHash = "sha256-Y8z+13XK40p36EYg5Nam8Ds8EW/OIStbpawl2sqOhrY=";
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
