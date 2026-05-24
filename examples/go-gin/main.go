// Run:
//   export CONFIGLY_API_KEY=cfly_...
//   go run .
//
// Then flip the "new_homepage" flag in the dashboard and reload http://localhost:3000.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	configly "github.com/paramahastha/configly/sdk/go"
)

func main() {
	cfg, err := configly.New(configly.Options{
		URL:         envOr("CONFIGLY_URL", "http://localhost:8080"),
		APIKey:      mustEnv("CONFIGLY_API_KEY"),
		Project:     "default",
		Environment: envOr("CONFIGLY_ENV", "dev"),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer cfg.Close()

	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		greeting := cfg.GetString("greeting", "Hello from Configly!")
		if cfg.IsEnabled("new_homepage", "", false) {
			c.String(http.StatusOK, "<h1>%s</h1><p>✨ New homepage is live — flip the flag to go back.</p>", greeting)
		} else {
			c.String(http.StatusOK, "<h1>%s</h1><p>Enable the <b>new_homepage</b> flag in the dashboard to see the new UI.</p>", greeting)
		}
	})

	r.GET("/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"greeting":         cfg.GetString("greeting", "Hello!"),
			"max_results":      cfg.GetInt("max_results", 20),
			"maintenance_mode": cfg.GetBool("maintenance_mode", false),
		})
	})

	addr := envOr("PORT", "3000")
	log.Printf("listening on :%s — manage configs at http://localhost:8080", addr)
	log.Fatal(r.Run(":" + addr))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("env var %s is required", key)
	}
	return v
}
