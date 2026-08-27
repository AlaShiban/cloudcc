package fuzz_test

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/fuzz"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// Cross-language IR equivalence.
//
// Everything downstream of detection -- intents, provider resolution, the IaC
// backend, deployment -- is supposed to be language-neutral. The only way to
// know that it still is, rather than that it was when the seam was extracted,
// is to write the same application twice and compare what comes out.
//
// This is the strongest available guard against the two frontends drifting.
// A change that teaches one language something the other does not know shows up
// here as a difference in the intent layer, which is exactly where it would
// otherwise go unnoticed until a Node program provisioned something subtly
// different from the Python one beside it.

// The same application, written twice. Same ids, same capabilities, same
// routes; nothing else is meant to survive into the IR.
const pythonSource = `from pathlib import Path

import boto3
from redis import Redis
from sqlalchemy import create_engine
from fastapi import FastAPI, HTTPException

import cloudcompiler as cloudcc

items = cloudcc.persist(boto3.resource("dynamodb").Table("items"), id="itemCache")
docs = cloudcc.persist(Path("./docs"), id="itemDocs")
signing = cloudcc.persist(cloudcc.Secret(), id="signingKey")
cache = cloudcc.persist(Redis(), id="sessions")
db = cloudcc.persist(create_engine("postgresql://localhost/shop"), id="shopdb")
events = cloudcc.persist(cloudcc.Topic(subscribers="many", ordering="none"), id="itemEvents")

LEVEL = cloudcc.config_value("log_level", default="info")

app = FastAPI()
cloudcc.expose(app, id="shop-api")


@app.get("/health")
def health() -> dict:
    return {"status": "ok"}


@app.get("/items/{item_id}")
def read_item(item_id: str) -> dict:
    found = items.get(item_id)
    if found is None:
        raise HTTPException(status_code=404, detail="missing")
    return found


@app.put("/items/{item_id}")
def write_item(item_id: str, payload: dict) -> dict:
    items.put(item_id, payload)
    return {"ok": True}
`

const nodeSource = `import { Secret, Topic, configValue, expose, persist } from "@cloudcompiler/sdk";
import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { S3Client } from "@aws-sdk/client-s3";
import Redis from "ioredis";
import { Pool } from "pg";
import express from "express";

const items = persist(new DynamoDBClient({}), { id: "itemCache" });
const docs = persist(new S3Client({}), { id: "itemDocs" });
const signing = persist(new Secret(), { id: "signingKey" });
const cache = persist(new Redis(), { id: "sessions" });
const db = persist(new Pool({ connectionString: "postgresql://localhost/shop" }), { id: "shopdb" });
const events = persist(new Topic({ subscribers: "many", ordering: "none" }), { id: "itemEvents" });

const LEVEL = configValue("log_level", { default: "info" });

const app = express();
app.use(express.json());
expose(app, { id: "shop-api" });

app.get("/health", async (req, res) => {
  res.json({ status: "ok" });
});

app.get("/items/:itemId", async (req, res) => {
  const found = await items.get(req.params.itemId);
  if (found === null) {
    res.status(404).json({ detail: "missing" });
    return;
  }
  res.json(found);
});

app.put("/items/:itemId", async (req, res) => {
  await items.put(req.params.itemId, req.body);
  res.json({ ok: true });
});

export { app };
`

// languageSpecific are the intent fields that are *supposed* to differ. Every
// other difference is drift.
//
// The route parameter name is in here for an honest reason: Python spells a
// path parameter {item_id} and Express spells it :itemId, and neither is
// convertible to the other without renaming the handler's argument. The path
// *shape* still has to match, which is what the comparison below checks after
// normalising parameter names away.
var languageSpecific = map[string]bool{
	"language":     true,
	"runtime":      true,
	"handler":      true,
	"artifact":     true,
	"entry_module": true,
	"entrypoints":  true,
	"files":        true,
	"asgi_app":     true,
	"library":      true, // redis-py vs ioredis: the same capability, different client
}

func TestTheTwoFrontendsProduceTheSameIntents(t *testing.T) {
	py := &fuzz.Program{
		Seed:  0,
		Name:  "crosslang",
		Files: map[string]string{"app.py": pythonSource},
		// The compute type is stated so neither language falls back to a
		// different default and makes this pass for the wrong reason.
		Config: "app: crosslang\nprovider: aws\ndefaults:\n  execution_unit:\n    type: function\n",
	}
	node := &fuzz.Program{
		Seed: 0,
		Name: "crosslang",
		Files: map[string]string{
			"app.js": nodeSource,
			"package.json": `{"name":"crosslang","private":true,"type":"module",` +
				`"dependencies":{"express":"^4.21.2"}}` + "\n",
		},
		Config: "app: crosslang\nprovider: aws\ndefaults:\n  execution_unit:\n    type: function\n",
	}

	pyBuilt := build(t, py)
	nodeBuilt := build(t, node)

	pyIntents := intentMap(t, pyBuilt.dump)
	nodeIntents := intentMap(t, nodeBuilt.dump)

	pyKeys, nodeKeys := sortedKeysOf(pyIntents), sortedKeysOf(nodeIntents)
	if !sameStrings(pyKeys, nodeKeys) {
		t.Fatalf("the two frontends produced different intents:\n  python: %v\n  node:   %v",
			pyKeys, nodeKeys)
	}

	for _, key := range pyKeys {
		a := normaliseIntent(pyIntents[key])
		b := normaliseIntent(nodeIntents[key])
		aJSON, _ := json.MarshalIndent(a, "", "  ")
		bJSON, _ := json.MarshalIndent(b, "", "  ")
		if string(aJSON) != string(bJSON) {
			t.Errorf("intent %s differs between the frontends:\n--- python ---\n%s\n--- node ---\n%s",
				key, aJSON, bJSON)
		}
	}
}

// intentMap keys every intent by kind and id.
func intentMap(t *testing.T, dump ir.Dump) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, in := range dump.Intents {
		raw, err := json.Marshal(in.Payload)
		if err != nil {
			t.Fatalf("intent payload is not JSON: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("intent payload is not an object: %v", err)
		}
		out[in.Key.Kind+"/"+in.Key.ID] = payload
	}
	return out
}

// normaliseIntent drops the fields that are meant to differ and rewrites route
// parameter names, which each language spells in its own idiom.
func normaliseIntent(payload map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range payload {
		if languageSpecific[k] {
			continue
		}
		if k == "routes" {
			out[k] = normaliseRoutes(v)
			continue
		}
		out[k] = v
	}
	return out
}

// normaliseRoutes replaces every {param} with {} so that {item_id} and
// {itemId} compare equal. The number and position of parameters still has to
// match, which is the part that would actually break a deployment.
func normaliseRoutes(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		route, ok := item.(map[string]any)
		if !ok {
			continue
		}
		verb, _ := route["verb"].(string)
		path, _ := route["path"].(string)
		out = append(out, verb+" "+maskParams(path))
	}
	sort.Strings(out)
	return out
}

func maskParams(path string) string {
	var b []rune
	inParam := false
	for _, r := range path {
		switch {
		case r == '{':
			inParam = true
			b = append(b, '{')
		case r == '}':
			inParam = false
			b = append(b, '}')
		case !inParam:
			b = append(b, r)
		}
	}
	return string(b)
}

func sortedKeysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
