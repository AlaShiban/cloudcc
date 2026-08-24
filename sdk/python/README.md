# cloudcompiler

The hint SDK for [CloudCompiler](../../README.md).

```python
from fastapi import FastAPI
import cloudcompiler as cc

app = FastAPI()
pets = cc.persist_kv("petsByOwner")
cc.expose(app, id="pet-api")
```

`cc compile` reads these calls statically and rewrites them, in a copy of your
source, into real AWS clients. Nothing in this package ever talks to a cloud:
outside the compiler the calls return small local emulations so `uvicorn
app:app` keeps working on your laptop.

Arguments must be literals — the compiler never executes your program, so
`cc.persist_kv(name)` is a compile error that points at the argument.
