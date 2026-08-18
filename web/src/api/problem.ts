import { z } from "zod";

import { canonicalUuidPattern } from "../shared/codec";

export interface ApiProblem {
  errorCode: string;
  message: string;
  retryable: boolean;
  workflowRunId?: string;
  details?: Readonly<Record<string, unknown>>;
}

export class ProblemValidationError extends Error {
  constructor() {
    super("API Problem 响应结构无效");
    this.name = "ProblemValidationError";
  }
}

const nonEmptyStringSchema = z.string().refine((value) => value.trim() !== "");
const problemSchema = z.strictObject({
  error_code: nonEmptyStringSchema,
  message: nonEmptyStringSchema,
  retryable: z.boolean(),
  workflow_run_id: nonEmptyStringSchema.regex(canonicalUuidPattern).optional(),
  details: z.record(z.string(), z.unknown()).optional(),
});

/** 严格解码通用 Problem，不在校验错误中保留原始响应或敏感 details。 */
export const decodeApiProblem = (value: unknown): ApiProblem => {
  const result = problemSchema.safeParse(value);
  if (!result.success) throw new ProblemValidationError();
  return {
    errorCode: result.data.error_code,
    message: result.data.message,
    retryable: result.data.retryable,
    ...(result.data.workflow_run_id === undefined ? {} : { workflowRunId: result.data.workflow_run_id }),
    ...(result.data.details === undefined ? {} : { details: result.data.details }),
  };
};
