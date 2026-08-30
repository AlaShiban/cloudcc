# A TypeScript container on Kubernetes

The mirror of `examples/k8s-web`, and the smallest example in the repository:
one unit, no stores, no message. It exists to make one claim, twice.

**Nothing in the program says Kubernetes.** No manifest, no Deployment, no
Service, no image reference. `cloudcc.yaml` says `platform: kubernetes` and the
same file compiles to a Deployment behind a Service; remove that line and it
compiles to a Fargate task behind a load balancer, with `src/web.ts` untouched.

**Nothing in the program says TypeScript, either — but the packaging does.** A
container runs the program rather than a handler the platform calls, so "can
`node` load this?" is a real question there in a way it is not for a function.
It cannot: `node web.ts` is not a thing. So the unit is bundled by esbuild on
the way into the image, exactly as a function is, and the image carries one
`index.mjs` whichever language it came from.

It deliberately reaches no store. A pod's AWS identity comes from IRSA, which
`cloudcc` does not emit yet, so a unit that did reach one would be warned at
compile time — and an example is a bad place to demonstrate a gap.

```bash
npx tsx src/web.ts                       # as written
./tests/e2e/kubernetes.sh k8s-web-ts     # against a real k3s cluster
```
