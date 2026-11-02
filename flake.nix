{
  description = "allowmaild - narrowly scoped email-sending daemon with a recipient allowlist";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        allowmaild = pkgs.buildGoModule rec {
          pname = "allowmaild";
          version = pkgs.lib.trim (builtins.readFile ./VERSION);

          src = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = pkgs.lib.fileset.unions [
              ./go.mod
              ./go.sum
              ./cmd
              ./internal
            ];
          };

          vendorHash = "sha256-nJfpoMD5D1NYY9RgSOAVqkTKZOVgXSx13e2WgQ8awM8=";

          subPackages = [ "cmd/allowmaild" ];
          env.CGO_ENABLED = "0";
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];

          meta = {
            description = "Email-sending daemon that can only deliver to an allowlisted set of recipients";
            homepage = "https://github.com/kadel/allowmaild";
            mainProgram = "allowmaild";
          };
        };
        default = allowmaild;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            # For the openclaw-plugin/ TypeScript subproject.
            nodejs_24
          ];
        };
      });
    };
}
