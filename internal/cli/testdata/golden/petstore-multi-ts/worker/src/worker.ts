// The background execution unit.
//
// It shares shared/store.ts with the api unit, which is what makes both units
// resolve to the same DynamoDB table and the same queue.
//
// What it does *not* share is shared/catalogue.ts. Permissions and environment
// are both derived from what a unit bundles, so a worker that never imports the
// catalogue is a worker with no access to the api's database or its cache --
// least privilege as a consequence of the import graph rather than as a list
// somebody maintains.
//
// It has a relational store of its own, in shared/ledger.ts, reached through a
// `pg` Pool -- the other half of the pair the api's Knex instance belongs to.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccFs from "../_cloudcc_runtime/fs.js";

import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";

import { audited, record } from "@/shared/ledger";
import { stamp } from "@/shared/signing";
import { events, readPet, summarize } from "@/shared/store";

undefined;

const BUCKET = "petAudit" as const;

// An S3Client is not bound to a bucket -- the name travels in each command --
// so the program keeps writing the name it chose and the shim rewrites it to
// the provisioned one.
const audit = _cloudccFs.connect("petAudit") as S3Client;

interface PetEvent {
  action?: string;
  id?: string;
  summary?: string;
}

async function onPetEvent(message: PetEvent) {
  const petId = message.id ?? "";
  const pet = (await readPet(petId)) ?? { name: "unnamed" };
  const summary = summarize(pet);
  // Signed with the managed secret, which is the only thing this unit needs
  // that the api does not have.
  const signature = await stamp(petId, summary);

  await audit.send(
    new PutObjectCommand({
      Bucket: BUCKET,
      Key: `${petId}.txt`,
      Body: `${summary}\nsigned: ${signature}\n`,
      ContentType: "text/plain",
    }),
  );

  // The same fact in the ledger, where it can be counted and queried. The
  // bucket holds the document; the database holds the index.
  const revision = await record(petId, summary, signature);
  return { audited: petId, revision, ledger: await audited() };
}

events.subscribe(onPetEvent);
