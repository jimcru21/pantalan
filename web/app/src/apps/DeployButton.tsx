import { useDeploy } from "@/api/deployments";
import type { Deployment } from "@/api/generated/domain";

/**
 * Deploys the App's branch. It stays enabled while a deploy is on its way:
 * every click queues one more Deployment, which the server runs in turn.
 * A refused deploy says why next to the button. `onDeployed` gets each
 * queued Deployment.
 */
export function DeployButton({
  appID,
  onDeployed,
}: {
  appID: string;
  onDeployed?: (deployment: Deployment) => void;
}) {
  const deploy = useDeploy(appID);
  return (
    <span className="flex items-center gap-3">
      {deploy.error && (
        <span role="alert" className="text-[13px] text-destructive">
          Deploy failed: {deploy.error.message}
        </span>
      )}
      <button
        type="button"
        onClick={() => deploy.mutate({}, { onSuccess: onDeployed })}
        className="cursor-pointer rounded-md bg-primary px-3 py-[5px] font-semibold text-primary-foreground"
      >
        Deploy
      </button>
    </span>
  );
}
