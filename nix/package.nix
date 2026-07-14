{
  lib,
  buildGoModule,
  gitignoreSource,
  sqlite,
}:
let
  version = "0.17.1";
in
buildGoModule {
  pname = "msgvault";
  inherit version;

  src = gitignoreSource ../.;

  vendorHash = "sha256-6HSmNw/CTjtO8RxFNVwupGm2vUifoJGX2slxbimcOVE=";
  proxyVendor = true;

  subPackages = [ "cmd/msgvault" ];

  # mattn/go-sqlite3 and sqlite-vec both link C code. buildGoModule defaults
  # CGO_ENABLED to 1, but be explicit.
  env.CGO_ENABLED = 1;

  # sqlite-vec-go-bindings does `#include "sqlite3.h"` but ships no sqlite
  # source — provide the system header via buildInputs.
  buildInputs = [ sqlite ];

  tags = [
    "fts5"
    "sqlite_vec"
  ];

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
