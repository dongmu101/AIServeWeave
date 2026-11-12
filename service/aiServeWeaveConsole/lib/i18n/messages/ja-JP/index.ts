import core from "./core.ts";
import common from "./common.ts";
import auth from "./auth.ts";
import tenant from "./tenant.ts";
import operator from "./operator.ts";
import docs from "./docs.ts";
import site from "./site.ts";
const messages = { ...core, ...common, ...auth, ...tenant, ...operator, ...docs, ...site };
export default messages;
