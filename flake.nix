{
  description = "Lightweight real-time Gmail attachment archiver and API query service";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
    in
    flake-utils.lib.eachSystem supportedSystems (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "gmail-archiver";
          version = "0.1.0";
          src = ./.;

          subPackages = [ "cmd/server" ];

          # Set CGO_ENABLED=0 for a pure Go static binary
          env = {
            CGO_ENABLED = 0;
          };
          ldflags = [ "-s" "-w" ];

          vendorHash = "sha256-G4PlRUToaEeZs8N/a54KDhGGWYnaShgAXHuL2iPO+hY=";

          postInstall = ''
            if [ -f "$out/bin/server" ]; then
              mv "$out/bin/server" "$out/bin/gmail-archiver"
            fi
          '';

          meta = with pkgs.lib; {
            description = "Lightweight Gmail attachment archiver with IMAP IDLE and REST API";
            homepage = "https://github.com/dot/gmail-archiver";
            license = licenses.mit;
            maintainers = [ ];
            mainProgram = "gmail-archiver";
          };
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/gmail-archiver";
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            sqlite
          ];
        };
      }
    ) // {
      # [NixOS Module]
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.gmail-archiver;
        in
        {
          options.services.gmail-archiver = {
            enable = lib.mkEnableOption "Gmail Archiver daemon";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              description = "The gmail-archiver package to use.";
            };

            imapServer = lib.mkOption {
              type = lib.types.str;
              default = "imap.gmail.com:993";
              description = "IMAP server address host:port.";
            };

            imapUser = lib.mkOption {
              type = lib.types.str;
              description = "Gmail address / username.";
            };

            passwordFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              description = "Path to file containing Google App Password.";
            };

            dataDir = lib.mkOption {
              type = lib.types.path;
              default = "/var/lib/gmail-archiver";
              description = "Directory to store SQLite metadata and attachments.";
            };

            httpPort = lib.mkOption {
              type = lib.types.port;
              default = 8080;
              description = "Port for REST HTTP API.";
            };

            apiKeyFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              description = "Optional path to file containing API Key for authentication.";
            };

            environmentFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              description = "Path to environment file containing secrets (e.g. IMAP_PASSWORD, API_KEY).";
            };
          };

          config = lib.mkIf cfg.enable {
            systemd.services.gmail-archiver = {
              description = "Gmail Attachment Archiver and API Service";
              wantedBy = [ "multi-user.target" ];
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];

              environment = {
                IMAP_SERVER = cfg.imapServer;
                IMAP_USER = cfg.imapUser;
                DATA_DIR = cfg.dataDir;
                HTTP_PORT = toString cfg.httpPort;
              };

              script =
                let
                  hasLegacyFiles = (cfg.passwordFile != null) || (cfg.apiKeyFile != null);
                in
                if hasLegacyFiles then ''
                  ${lib.optionalString (cfg.passwordFile != null) ''
                    export IMAP_PASSWORD="$(cat "${cfg.passwordFile}")"
                  ''}
                  ${lib.optionalString (cfg.apiKeyFile != null) ''
                    export API_KEY="$(cat "${cfg.apiKeyFile}")"
                  ''}
                  exec ${cfg.package}/bin/gmail-archiver
                '' else ''
                  exec ${cfg.package}/bin/gmail-archiver
                '';

              serviceConfig = {
                Type = "simple";
                Restart = "always";
                RestartSec = "10s";

                EnvironmentFile = lib.optional (cfg.environmentFile != null) cfg.environmentFile;

                # Security hardening & StateDirectory
                DynamicUser = true;
                StateDirectory = "gmail-archiver";
                WorkingDirectory = cfg.dataDir;

                CapabilityBoundingSets = "";
                NoNewPrivileges = true;
                ProtectSystem = "strict";
                ProtectHome = true;
                PrivateTmp = true;
                PrivateDevices = true;
                ProtectKernelTunables = true;
                ProtectControlGroups = true;
                RestrictNamespaces = true;
                RestrictRealtime = true;
                MemoryDenyWriteExecute = true;
              };
            };
          };
        };
    };
}
