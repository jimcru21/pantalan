// The App Log on an App's page: what its live container prints, followed
// live from its last 200 lines.
import { useEffect, useRef } from "react";
import { AppLogNoLiveDeployment, AppLogReplaced } from "@/api/generated/httpapi";
import { appPaths } from "@/api/paths";
import { useLogStream, useRestartLogStream } from "@/api/streams";
import { buttonClass } from "@/components/form";
import { Panel } from "@/components/panel";
import { LogView } from "@/logs/LogView";

/**
 * Follows the App Log of the App with ID `appId`. The stream closes when
 * the page goes. `liveID` is the live Deployment the page knows of: when
 * the server said there was none and one goes live, the log follows it.
 */
export function AppLog({ appId, liveID }: { appId: string; liveID?: string }) {
  const url = appPaths.appLogStream(appId);
  const stream = useLogStream(url);
  const followAgain = useRestartLogStream(url);
  const ended = stream.state === "done" ? stream.done : undefined;

  const knownLive = useRef(liveID);
  useEffect(() => {
    const wentLive = liveID !== undefined && liveID !== knownLive.current;
    knownLive.current = liveID;
    if (wentLive && ended === AppLogNoLiveDeployment) followAgain();
  }, [liveID, ended, followAgain]);

  return (
    <Panel title="App Log">
      <LogView
        stream={stream}
        label="App Log"
        end={
          ended === AppLogNoLiveDeployment ? (
            "No live Deployment, so nothing to follow. Deploy the App to see what it prints."
          ) : ended === AppLogReplaced ? (
            <span className="flex flex-wrap items-center gap-3">
              A new Deployment replaced the container this log was following.
              <button type="button" className={buttonClass.small} onClick={followAgain}>
                Follow the new Deployment
              </button>
            </span>
          ) : undefined
        }
      />
    </Panel>
  );
}
