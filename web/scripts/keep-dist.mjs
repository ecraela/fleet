import { writeFileSync } from "node:fs";
// A fresh source checkout must still compile before its console is built.
writeFileSync(new URL("../../internal/webui/dist/.keep", import.meta.url), "");
