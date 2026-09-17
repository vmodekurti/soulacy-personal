<script>
  // GenieMark — the face of the thing you talk to.
  //
  // A solid core with an arc around it and a spark breaking away. The core is
  // what stays: your gateway, running on your own machine. The arc is presence
  // — it is around you rather than in front of you. The spark is the bit that
  // leaves and does something.
  //
  // What it is not, deliberately: a lamp. A lamp is a joke that stops being
  // funny on the second day. Nor the four-point AI sparkle every product now
  // uses, and not the Soulacy droplet either — the brand mark sits forty
  // pixels above this in the sidebar, and two droplets read as a duplicate.
  //
  // The spark earns its place. Without it the arc's gap reads as a loading
  // spinner; with it the gap is obviously deliberate. Three elements is the
  // ceiling: this has to survive 16px, where a fourth detail turns to mush.
  export let size = 20
  export let gradient = false
  export let title = ''

  // Unique per instance. Two gradients sharing an id on one page make the
  // second silently adopt the first.
  const uid = 'genie-' + Math.random().toString(36).slice(2, 9)
</script>

<svg
  class="genie-mark"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  role={title ? 'img' : 'presentation'}
  aria-hidden={title ? undefined : 'true'}
  aria-label={title || undefined}
>
  {#if title}<title>{title}</title>{/if}
  {#if gradient}
    <defs>
      <linearGradient id={uid} x1="4" y1="22" x2="20" y2="2" gradientUnits="userSpaceOnUse">
        <stop offset="0" stop-color="#8b85ff" />
        <stop offset="1" stop-color="#4ad6c4" />
      </linearGradient>
    </defs>
  {/if}

  <path
    d="M15.6 4.6A8.6 8.6 0 1 0 19.7 14"
    stroke={gradient ? `url(#${uid})` : 'currentColor'}
    stroke-width="2.6"
    stroke-linecap="round"
  />
  <circle cx="12" cy="12" r="3.9" fill={gradient ? `url(#${uid})` : 'currentColor'} />
  <circle cx="19.1" cy="5.1" r="2.2" fill={gradient ? `url(#${uid})` : 'currentColor'} />
</svg>

<style>
  .genie-mark { display: block; flex: none; }
</style>
