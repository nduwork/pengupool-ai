// Error codes from docs/protocol/v1.md section 2 and internal/protocol/errors.go. They are the
// stable, machine-readable part of every error response; the message text is not.
export const Codes = {
  InvalidArgument: "invalid_argument",
  Unauthorized: "unauthorized",
  Forbidden: "forbidden",
  NotFound: "not_found",
  Conflict: "conflict",
  Unavailable: "unavailable",
  IncompatibleVersion: "incompatible_version",
  WorkspaceBusy: "workspace_busy",
  HarnessUnavailable: "harness_unavailable",
  ProviderTimeout: "provider_timeout",
  ProviderRefused: "provider_refused",
  LowConfidence: "low_confidence",
  NoDecider: "no_decider",
  OwnerWork: "owner_work",

  TruncatedFrame: "protocol.truncated_frame",
  FrameTooLarge: "protocol.frame_too_large",
  UnknownFrameType: "protocol.unknown_frame_type",
  InvalidChannel: "protocol.invalid_channel",
  InvalidUTF8: "protocol.invalid_utf8",
  Unsupported: "protocol.unsupported",
} as const;

export type ErrorCode = (typeof Codes)[keyof typeof Codes];

/** A protocol error carrying the stable code the daemon uses on the wire. */
export class ProtocolError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(`${code}: ${message}`);
    this.name = "ProtocolError";
    this.code = code;
  }
}

export function errorf(code: string, message: string): ProtocolError {
  return new ProtocolError(code, message);
}

export function isProtocolError(err: unknown): err is ProtocolError {
  return err instanceof ProtocolError;
}
