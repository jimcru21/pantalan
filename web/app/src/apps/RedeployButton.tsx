import { useNavigate } from "react-router";
import { useDeploy } from "@/api/deployments";
import { buttonClass } from "@/components/form";

/**
 * Rolls back: starts a new Deployment of the exact commit `deploymentId`
 * resolved, then lands on the new Deployment's page so the Admin watches
 * it go live. The earlier Deployment stays as it was in the history. A
 * refused redeploy says why next to the button.
 */
export function RedeployButton({ appId, deploymentId }: { appId: string; deploymentId: string }) {
  const deploy = useDeploy(appId);
  const navigate = useNavigate();
  const redeploy = () =>
    deploy.mutate(
      { redeployOf: deploymentId },
      {
        onSuccess: (dep) =>
          void navigate(
            `/apps/${encodeURIComponent(appId)}/deployments/${encodeURIComponent(dep.id)}`,
          ),
      },
    );
  return (
    <span className="flex items-center gap-3">
      {deploy.error && (
        <span role="alert" className="text-[13px] text-destructive">
          Redeploy failed: {deploy.error.message}
        </span>
      )}
      <button
        type="button"
        onClick={redeploy}
        disabled={deploy.isPending}
        className={`${buttonClass.small} disabled:cursor-default disabled:opacity-70`}
      >
        Redeploy this commit
      </button>
    </span>
  );
}
