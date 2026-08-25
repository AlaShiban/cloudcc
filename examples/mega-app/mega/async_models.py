"""Tortoise ORM models.

Named by `tortoise_config` in mega/orm.py as a dotted module path, which is a
small detail with a real consequence: the compiler has to follow that string to
know this module belongs in the bundle. A module reachable only through a
configuration string is invisible to import-graph analysis, and a unit that
ships without it fails at `Tortoise.init` rather than at compile time.

So: any capability whose configuration names a module by string must contribute
that module to the unit's closure. The same applies to Django's ROOT_URLCONF
and INSTALLED_APPS, and to Celery's `include=`.
"""

from tortoise import fields
from tortoise.models import Model


class Event(Model):
    id = fields.UUIDField(primary_key=True)
    order_id = fields.CharField(max_length=64)
    kind = fields.CharField(max_length=32)
    at = fields.DatetimeField(auto_now_add=True)
