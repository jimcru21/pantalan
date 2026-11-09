// A tile's sparkline: Recharts, used directly (shadcn/ui's chart wrapper
// renders a <style> element, which the CSP blocks). Colours come from
// CSS classes; Recharts' own positioning goes through the `style` prop,
// which writes through the CSSOM and so is allowed.
import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatPercent } from "@/lib/format";

// The sampler keeps 10 minutes, one sample every 5 s. A ring that isn't
// full yet is drawn against the right edge, newest last, as before.
const historyLen = 120;

/** A ratio series (0..1, oldest first) that shows the value under the pointer. */
export function Sparkline({ label, values }: { label: string; values: number[] }) {
  const offset = historyLen - values.length;
  const data = values.map((value, i) => ({ slot: offset + i, value }));
  return (
    <div className="mt-1 h-7 w-full [&_.recharts-dot]:fill-primary [&_.recharts-dot]:stroke-card [&_.recharts-line-curve]:stroke-primary [&_.recharts-tooltip-cursor]:stroke-border">
      <ResponsiveContainer width="100%" height="100%" initialDimension={{ width: 160, height: 28 }}>
        <LineChart data={data} margin={{ top: 2, right: 0, bottom: 2, left: 0 }} title={label}>
          <XAxis dataKey="slot" type="number" domain={[0, historyLen - 1]} hide />
          <YAxis type="number" domain={[0, 1]} hide />
          <Line
            dataKey="value"
            type="linear"
            strokeWidth={1.5}
            dot={false}
            activeDot={{ r: 3 }}
            isAnimationActive={false}
          />
          <Tooltip
            isAnimationActive={false}
            content={({ active, payload }) => {
              const value = payload?.[0]?.value;
              if (!active || typeof value !== "number") return null;
              return (
                <span className="rounded-md bg-foreground px-2 py-1 text-xs whitespace-nowrap text-background tabular-nums">
                  {formatPercent(value)}
                </span>
              );
            }}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
