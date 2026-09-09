package main

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	_ "github.com/enzom/jungle-gaming/docs/swagger"
	"github.com/enzom/jungle-gaming/internal/composition"
)

// @title									Distributed Wagering API
// @version								1.0.0
// @description							API for wallets and distributed, idempotent wager processing.
// @host									localhost:8080
// @BasePath								/
// @schemes								http
// @securityDefinitions.oauth2.accessCode	InternalOAuth
// @authorizationUrl						http://localhost:8081/realms/gaming/protocol/openid-connect/auth
// @tokenUrl								http://localhost:8081/realms/gaming/protocol/openid-connect/token
// @securityDefinitions.oauth2.accessCode	ProviderOAuth
// @authorizationUrl						http://localhost:8081/realms/gaming/protocol/openid-connect/auth
// @tokenUrl								http://localhost:8081/realms/gaming/protocol/openid-connect/token
func main() {
	fx.New(
		composition.Module(),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: logger}
			fxLogger.UseLogLevel(slog.LevelDebug)
			return fxLogger
		}),
	).Run()
}
