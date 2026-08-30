// The subscriber, which has to be a function.
//
// It used to be part of the reporter, and that was a bug: the reporter is a
// container, a topic delivery is *pushed* to a function, and nothing polls on a
// container's behalf -- so the subscription was never created and every message
// went nowhere. It looked fine because nothing asserted the audit records
// existed.
//
// The compiler refuses that arrangement now, which is why this unit exists. It
// is the same handler, in the one shape that can receive a message.


import { events, writeDoc, type ItemEvent } from "@/stores";

undefined;

async function onItemEvent(message: ItemEvent) {
  await writeDoc(`audit/${message.id}.txt`, message.action);
  return { audited: message.id };
}

events.subscribe(onItemEvent);
