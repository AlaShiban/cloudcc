"""The subscriber, which has to be a function.

It used to be part of the reporter, and that was a bug: the reporter is a
container, a topic delivery is *pushed* to a function, and nothing polls on a
container's behalf -- so the subscription was never created and every message
went nowhere. It looked fine because nothing asserted the audit records
existed.

The compiler refuses that arrangement now, which is why this unit exists. It is
the same handler, in the one shape that can receive a message.
"""

import cloudcompiler as cloudcc

from stores import events, write_doc

cloudcc.execution_unit(id="auditor")


def on_item_event(message: dict):
    write_doc(f"audit/{message['id']}.txt", message["action"].encode("utf-8"))
    return {"audited": message["id"]}


events.subscribe(on_item_event)
