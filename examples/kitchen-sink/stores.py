"""Every stateful capability, declared once and shared by both units."""

import cloudcompiler as cloudcc

catalogue = cloudcc.persist_kv("catalogue")
docs = cloudcc.persist_fs("itemDocs")
signing_key = cloudcc.persist_secret("signingKey")
db = cloudcc.persist_orm("shopdb", models=["Item", "Order"])
cache = cloudcc.persist_redis("itemCache")
events = cloudcc.pubsub_topic("itemEvents")
