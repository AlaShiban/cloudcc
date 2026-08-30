"""The menu, and the arithmetic over it.

Nothing here is a capability. It is ordinary shared code, imported by several
units, and it is in the example on purpose: most of a program is this, and the
units that need it each get a copy in their bundle without anybody saying so.
"""

#: Fallback prices in cents, used when the pricing table has no row for a sku.
#: A real service would not have these; this one does so that the example is
#: runnable before anything has been seeded.
CATALOG = {
    "margherita": 1200,
    "pepperoni": 1450,
    "garlic-bread": 500,
    "cola": 300,
}

#: Delivery is flat, which is a business decision rather than a technical one,
#: and lives here for the same reason the catalogue does.
DELIVERY_CENTS = 349


def line_total(unit_cents: int, qty: int) -> int:
    return unit_cents * int(qty)


def order_total(line_cents: list[int]) -> int:
    return sum(line_cents) + DELIVERY_CENTS


def describe(items: list[dict]) -> str:
    """A one-line summary of a basket, for notifications and audit records."""
    return ", ".join(f"{item.get('qty', 1)}x {item.get('sku', '?')}" for item in items)
