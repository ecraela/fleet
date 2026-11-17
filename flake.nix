{
  description = "Ecraela Fleet development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26
            nodejs_24
            pnpm
            python3
            postgresql_18
            age
            docker-client
            docker-compose
            gnumake
            git
            curl
            pkg-config
            openssl
          ];

          POSTGRES_BIN = "${pkgs.postgresql_18}/bin";
        };
      }
    );
}
