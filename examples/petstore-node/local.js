// Runs the app as written, with no cloud account: the SDK's hint calls return
// local emulations, so `node local.js` just works.
import { app } from "./server.js";

const port = Number(process.env.PORT ?? 3000);
app.listen(port, () => console.log(`listening on ${port}`));
