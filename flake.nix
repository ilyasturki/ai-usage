{
  description = "ai-usage — Claude and Codex subscription usage in the terminal";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # The repo-root VERSION file is the single source of truth for the
      # version, shared with the Go build (injected via ldflags below) and the
      # justfile.
      version = nixpkgs.lib.fileContents ./VERSION;
      forAllSystems = nixpkgs.lib.genAttrs [
        "x86_64-linux"
        "aarch64-linux"
      ];
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          default = ai-usage;

          ai-usage = pkgs.buildGoModule {
            pname = "ai-usage";
            inherit version;
            src = ./.;
            # Pure standard library: no dependencies to vendor.
            vendorHash = null;
            ldflags = [
              "-s"
              "-w"
              "-X ai-usage/internal/version.Version=${version}"
            ];
            meta = {
              description = "Claude and Codex subscription usage in the terminal";
              mainProgram = "ai-usage";
              license = pkgs.lib.licenses.mit;
              platforms = pkgs.lib.platforms.linux;
            };
          };
        }
      );

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/ai-usage";
        };
      });

      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt-tree);
    };
}
