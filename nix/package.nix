{
  lib,
  buildGoModule,
  gitignoreSource,
}:
let
  version = "0.17.1";
in
buildGoModule {
  pname = "msgvault";
  inherit version;

  src = gitignoreSource ../.;

  vendorHash = "sha256-DeARWk09ZvD4iOFWE8uM87sXKB31VsIf56IYoMib+u4=";
  proxyVendor = true;

  subPackages = [ "cmd/msgvault" ];

  # mattn/go-sqlite3 links C code. buildGoModule defaults CGO_ENABLED to 1,
  # but be explicit.
  env.CGO_ENABLED = 1;

  tags = [ "fts5" ];

  ldflags = [
    "-s"
    "-w"
    "-X go.kenn.io/msgvault/cmd/msgvault/cmd.Version=${version}"
  ];

  doCheck = false;

  meta = {
    description = "Offline Gmail archive with full-text search";
    homepage = "https://github.com/kenn-io/msgvault";
    license = lib.licenses.asl20;
    mainProgram = "msgvault";
  };
}
