"""Every stateful capability, declared once and shared by both units."""
# Injected by cc: runtime clients for this program's declared capabilities.
from _cc_runtime import fs as _cc_fs
from _cc_runtime import kv as _cc_kv
from _cc_runtime import orm as _cc_orm
from _cc_runtime import pubsub as _cc_pubsub
from _cc_runtime import redis_ as _cc_redis
from _cc_runtime import secret as _cc_secret



catalogue = _cc_kv.connect("catalogue")
docs = _cc_fs.connect("itemDocs")
signing_key = _cc_secret.connect("signingKey")
db = _cc_orm.connect("shopdb")
cache = _cc_redis.connect("itemCache")
events = _cc_pubsub.connect("itemEvents")
