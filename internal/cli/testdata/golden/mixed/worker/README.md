# Mixed-language example

One application, two units, two languages, one store.

* `api.js` is a Node HTTP unit, compiled to Lambda behind API Gateway v2.
* `worker.py` is a Python unit with no HTTP surface.
* Both declare `persist(..., id="petsByOwner")`, so both resolve to the *same*
  DynamoDB table and each gets its own environment binding.

Nothing in `cloudcc.yaml` mentions a language. A frontend is chosen per
execution unit from its entrypoint's extension, which is what makes this
ordinary rather than a special case.

```console
$ cloudcc examples/mixed -o compiled
```
