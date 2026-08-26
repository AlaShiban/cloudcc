"""The two topics, declared once and imported by everyone who uses them.

This is the other half of how units talk to each other, and the difference from
``cloudcc.remote`` is the whole point of having both:

* A **call** is a question. The caller waits, the answer comes back, and if the
  other unit is down the caller fails. ``storefront`` cannot price a basket
  without ``pricing``, so it calls it.

* A **message** is a statement. The publisher does not wait and does not learn
  who listened. An order having been placed is true whether or not anything
  reacts to it yet, and adding a fourth listener is a change to that listener
  alone.

Both topics fan out, so both resolve to SNS. Nothing here names SNS: the
requirements say many subscribers, no ordering guarantee, at-least-once, and
the compiler chooses from that -- which is what lets ``orderPlaced`` become a
FIFO queue later by changing the requirement rather than the code.
"""

import cloudcompiler as cloudcc

#: Fan-out, unordered, at-least-once: the defaults, spelled out because the
#: point of the topic carrying its requirements is that a reader can see them.
order_placed = cloudcc.persist(
    cloudcc.Topic(subscribers="many", ordering="none", delivery="at_least_once"),
    id="orderPlaced",
)

#: Same shape. A separate topic rather than a "type" field on the first,
#: because subscribers differ: dispatch listens to one and not the other, and a
#: subscriber that has to filter is a subscriber being woken up for nothing.
courier_assigned = cloudcc.persist(
    cloudcc.Topic(subscribers="many", ordering="none", delivery="at_least_once"),
    id="courierAssigned",
)
