// 16-point compass helpers for the GoTo page. The backend already labels each star's azimuth, but the
// alignment sky chart needs richer direction ticks than the 4-point map in SkyMap.vue.
const POINTS = [
  "N",
  "NNE",
  "NE",
  "ENE",
  "E",
  "ESE",
  "SE",
  "SSE",
  "S",
  "SSW",
  "SW",
  "WSW",
  "W",
  "WNW",
  "NW",
  "NNW",
];

// compass16 names an azimuth (degrees, N=0 increasing east) on the 16-point compass.
export function compass16(azDeg: number): string {
  const i = Math.round((((azDeg % 360) + 360) % 360) / 22.5) % 16;
  return POINTS[i];
}
