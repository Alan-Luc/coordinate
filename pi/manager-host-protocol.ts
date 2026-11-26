import { Type } from "@earendil-works/pi-ai";
import type { Static } from "typebox";
import { sessionTargetSchema, uuidPattern } from "./control-protocol.ts";

const strict = { additionalProperties: false };
export const launchFeaturesSchema = Type.Object({ features: Type.Object({ interactive_launch: Type.Literal(1), terminal_profile: Type.Literal("transparent-v1"), manager_host: Type.Literal(1), pending_manager: Type.Literal(1) }, strict) }, strict);
export const managerHostRequestSchema = Type.Object({ target: sessionTargetSchema }, strict);
export const managerHostResultSchema = Type.Object({ role: Type.Object({ schema: Type.Literal(1), role: Type.Literal("manager-host") }, strict) }, strict);
export const awaitingConversationSchema = Type.Object({ awaiting_conversation: Type.Object({ job_id: Type.String({ pattern: uuidPattern.source }) }, strict) }, strict);
export type ManagerHostRequest = Static<typeof managerHostRequestSchema>;
export type AwaitingConversation = Static<typeof awaitingConversationSchema>;
