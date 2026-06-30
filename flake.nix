{
  description = "ai-usage — Claude and Codex subscription usage in the terminal";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
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
            version = "0.1.0";
            src = ./.;
            # Pure standard library: no dependencies to vendor.
            vendorHash = null;
            ldflags = [
              "-s"
              "-w"
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
