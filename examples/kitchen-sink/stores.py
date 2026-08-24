"""Every stateful capability, declared once and shared by both units."""

import cloudcompiler as cc

catalogue = cc.persist_kv("catalogue")
docs = cc.persist_fs("itemDocs")
signing_key = cc.persist_secret("signingKey")
db = cc.persist_orm("shopdb", models=["Item", "Order"])
cache = cc.persist_redis("itemCache")
events = cc.pubsub_topic("itemEvents")
