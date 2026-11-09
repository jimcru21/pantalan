// Display formats for the API's raw values (bytes, ratios, seconds, RFC
// 3339 times), in the browser's locale through Intl.

const percent = new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 0 });
const percentTenths = new Intl.NumberFormat(undefined, {
  style: "percent",
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
const oneDecimal = new Intl.NumberFormat(undefined, {
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
const twoDecimals = new Intl.NumberFormat(undefined, {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});
const integer = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

/** A ratio as a whole percentage: 0.3 is "30%". */
export function formatPercent(ratio: number): string {
  return percent.format(ratio);
}

/** A ratio as a percentage with one decimal: 0.125 is "12.5%". */
export function formatPercentTenths(ratio: number): string {
  return percentTenths.format(ratio);
}

/** A load average: "0.42". */
export function formatLoad(load: number): string {
  return twoDecimals.format(load);
}

/** A count: "1,024". */
export function formatCount(n: number): string {
  return integer.format(n);
}

// Binary units, largest first. Intl has no unit for these (its "gigabyte"
// is decimal), so the number is Intl's and the unit is ours.
const byteUnits: [string, number][] = [
  ["TiB", 2 ** 40],
  ["GiB", 2 ** 30],
  ["MiB", 2 ** 20],
  ["KiB", 2 ** 10],
];

function unitFor(bytes: number): [string, number] {
  return byteUnits.find(([, size]) => bytes >= size) ?? ["B", 1];
}

/** Bytes in their own binary unit with one decimal: "25.0 GiB", "512 B". */
export function formatBytes(bytes: number): string {
  const [name, size] = unitFor(bytes);
  if (size === 1) return `${integer.format(bytes)} B`;
  return `${oneDecimal.format(bytes / size)} ${name}`;
}

/** Part and whole in the whole's unit: "6.0 of 8.0 GiB". */
export function formatBytesOf(part: number, whole: number): string {
  const [name, size] = unitFor(whole);
  return `${oneDecimal.format(part / size)} of ${oneDecimal.format(whole / size)} ${name}`;
}

const duration = new Intl.DurationFormat(undefined, { style: "narrow", minutesDisplay: "always" });

/** Days, hours and minutes, leaving out zero units: "12d 4h 17m", "3m". */
export function formatUptime(seconds: number): string {
  const minutes = Math.floor(seconds / 60);
  return duration.format({
    days: Math.floor(minutes / (24 * 60)),
    hours: Math.floor(minutes / 60) % 24,
    minutes: minutes % 60,
  });
}

const utcTime = new Intl.DateTimeFormat(undefined, {
  timeZone: "UTC",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});
const localTime = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});
const utcDate = new Intl.DateTimeFormat(undefined, { timeZone: "UTC", dateStyle: "medium" });

/** An RFC 3339 time as the UTC time of day: "10:15:30". */
export function formatUTCTime(rfc3339: string): string {
  return utcTime.format(new Date(rfc3339));
}

/** An RFC 3339 time as the browser's local time of day: "11:15:30". */
export function formatLocalTime(rfc3339: string): string {
  return localTime.format(new Date(rfc3339));
}

/** An RFC 3339 time as its UTC date: "Oct 20, 2026". */
export function formatUTCDate(rfc3339: string): string {
  return utcDate.format(new Date(rfc3339));
}

const utcDateTime = new Intl.DateTimeFormat(undefined, {
  timeZone: "UTC",
  dateStyle: "medium",
  timeStyle: "medium",
  hourCycle: "h23",
});

/** An RFC 3339 time as its UTC date and time: "Oct 20, 2026, 10:15:30". */
export function formatUTCDateTime(rfc3339: string): string {
  return utcDateTime.format(new Date(rfc3339));
}

const shortDuration = new Intl.DurationFormat(undefined, { style: "narrow" });

/** A short span in minutes and seconds, leaving out zero units: "1m 30s", "4s". */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return (
    shortDuration.format({
      hours: Math.floor(s / 3600),
      minutes: Math.floor(s / 60) % 60,
      seconds: s % 60,
    }) || "0s"
  );
}
