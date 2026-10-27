import type { ReactElement } from "react";

import mascot from "../../../assets/mascot.svg"; // the penguin, shared with the other empty states

/** The penguin in an empty state, small. */
export function Mascot({ className = "size-12" }: { className?: string }): ReactElement {
  return <img src={mascot} alt="" className={className} />;
}
