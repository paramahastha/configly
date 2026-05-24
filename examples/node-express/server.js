// Run:
//   export CONFIGLY_API_KEY=cfly_...
//   npm install && node server.js
//
// Then flip the "new_checkout" flag in the dashboard and watch the response change.
import express from "express";
import { Client } from "@configly/sdk";

const cfg = new Client({
  url: process.env.CONFIGLY_URL || "http://localhost:8080",
  apiKey: process.env.CONFIGLY_API_KEY || (() => { throw new Error("CONFIGLY_API_KEY is required"); })(),
  project: "default",
  environment: process.env.CONFIGLY_ENV || "dev",
});

await cfg.start();

const app = express();

app.get("/", (req, res) => {
  const userId = req.query.userId || "";
  res.json({
    greeting:    cfg.getString("greeting", "Hello from Configly!"),
    maxResults:  cfg.getInt("max_results", 20),
    maintenance: cfg.getBool("maintenance_mode", false),
    newCheckout: cfg.isEnabled("new_checkout", userId, false),
  });
});

const port = process.env.PORT || 3000;
app.listen(port, () => {
  console.log(`Listening on :${port} — manage configs at http://localhost:8080`);
});
