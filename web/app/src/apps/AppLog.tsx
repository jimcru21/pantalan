// The App Log on an App's page: what its live container prints, followed
// live from its last 200 lines.
import { useEffect, useRef } from "react";
import { useLogStream, useRestartLogStream } from "@/api/streams";
import { buttonClass } from "@/components/form";
import { Panel } from "@/components/panel";
import { LogView } from "@/logs/LogView";

// The done event's data: why the server ended the stream.
const replaced = "replaced"; // a new Deployment replaced the container
const noLiveDeployment = "no-live-deployment"; // there was nothing to follow

/**
 * Follows the App Log of the App with ID `appID`. The stream closes when
 * the page goes. `liveID` is the live Deployment the page knows of: when
 * the server said there was none and one goes live, the log follows it.
 */
export function AppLog({ appID, liveID }: { appID: string; liveID?: string }) {
  const url = `/api/v1/apps/${encodeURIComponent(appID)}/log/stream`;
  const stream = useLogStream(url);
  const followAgain = useRestartLogStream(url);
  const ended = stream.state === "done" ? stream.done : undefined;

  const knownLive = useRef(liveID);
  useEffect(() => {
    const wentLive = liveID !== undefined && liveID !== knownLive.current;
    knownLive.current = liveID;
    if (wentLive && ended === noLiveDeployment) followAgain();
  }, [liveID, ended, followAgain]);

  return (
    <Panel title="App Log">
      <LogView
        stream={stream}
        label="App Log"
        end={
          ended === noLiveDeployment ? (
            "No live Deployment, so nothing to follow. Deploy the App to see what it prints."
          ) : ended === replaced ? (
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
