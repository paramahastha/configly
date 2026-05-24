// One-shot recovery tool: adds a new "recovery" API key for the first admin user
// and prints it. Uses direct SQL to avoid the schema-init step that conflicts
// with the live server's WAL lock.
// Delete this file after use.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	defaultDB := os.Getenv("CONFIGLY_DB")
	if defaultDB == "" {
		defaultDB = "configly.db"
	}
	dbPath := flag.String("db", defaultDB, "path to configly SQLite database")
	flag.Parse()

	db, err := sql.Open("sqlite3", *dbPath+"?_busy_timeout=5000&mode=rwc")
	if err != nil {
		log.Fatal("open:", err)
	}
	defer db.Close()

	// Find first admin user.
	var adminID, adminEmail string
	row := db.QueryRow(`SELECT id, email FROM users WHERE role = 'admin' LIMIT 1`)
	if err := row.Scan(&adminID, &adminEmail); err != nil {
		log.Fatal("no admin user found:", err)
	}

	// Generate a new API key: "cfly_" + 48 random hex chars.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		log.Fatal("rand:", err)
	}
	rawKey := "cfly_" + hex.EncodeToString(raw)

	// Hash it (sha256, same as store.CreateAPIKey).
	sum := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(sum[:])

	keyID := uuid.NewString()
	prefix := rawKey[:8]

	_, err = db.Exec(
		`INSERT INTO api_keys(id, user_id, name, prefix, key_hash, created_at)
		 VALUES (?, ?, 'recovery', ?, ?, datetime('now'))`,
		keyID, adminID, prefix, keyHash,
	)
	if err != nil {
		log.Fatal("insert key:", err)
	}

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(" Configly key recovery")
	fmt.Println("------------------------------------------------------------")
	fmt.Printf(" Admin email: %s\n", adminEmail)
	fmt.Printf(" New API key: %s\n", rawKey)
	fmt.Println()
	fmt.Println(" SAVE THIS KEY. It is shown only once.")
	fmt.Println("============================================================")
	fmt.Println()
}
